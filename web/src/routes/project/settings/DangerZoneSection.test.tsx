/*
 * S37: the danger-zone typed confirmation gate. The delete button must stay disabled until
 * the typed value equals the project key exactly, the copy must name the live counts, and
 * the confirm string passed to the server must be the typed value (the server re-checks it).
 */
import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { DeleteProjectConfirm } from "./DangerZoneSection";

function subject(overrides: Partial<Parameters<typeof DeleteProjectConfirm>[0]> = {}) {
  const onDelete = vi.fn();
  const utils = render(
    <DeleteProjectConfirm
      projectKey="PAY"
      projectName="Payments"
      counts={{ tickets: 12, runs: 34, wiki_pages: 5 }}
      onDelete={onDelete}
      {...overrides}
    />,
  );
  return { onDelete, ...utils };
}

describe("DeleteProjectConfirm", () => {
  it("names the counts of what will go", () => {
    subject();
    const copy = screen.getByText(/Permanently delete Payments/).textContent ?? "";
    expect(copy).toContain("12 tickets");
    expect(copy).toContain("34 runs");
    expect(copy).toContain("5 wiki pages");
  });

  it("keeps the delete button disabled until the key is typed exactly", () => {
    const { onDelete } = subject();
    const button = screen.getByRole("button", { name: /Delete this project forever/ });
    const input = screen.getByLabelText("Type PAY to confirm deletion");

    expect((button as HTMLButtonElement).disabled).toBe(true);

    fireEvent.change(input, { target: { value: "PA" } });
    expect((button as HTMLButtonElement).disabled).toBe(true);

    fireEvent.change(input, { target: { value: "pay" } }); // case matters
    expect((button as HTMLButtonElement).disabled).toBe(true);

    fireEvent.change(input, { target: { value: "PAY" } });
    expect((button as HTMLButtonElement).disabled).toBe(false);

    fireEvent.click(button);
    expect(onDelete).toHaveBeenCalledWith("PAY");
  });

  it("does not fire onDelete from a click while unarmed", () => {
    const { onDelete } = subject();
    fireEvent.click(screen.getByRole("button", { name: /Delete this project forever/ }));
    expect(onDelete).not.toHaveBeenCalled();
  });

  it("stays disabled while the deletion is in flight", () => {
    subject({ busy: true });
    const button = screen.getByRole("button", { name: /Deleting…/ });
    expect((button as HTMLButtonElement).disabled).toBe(true);
  });
});
