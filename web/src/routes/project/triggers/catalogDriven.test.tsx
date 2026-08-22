/*
 * S29 acceptance (and the S32 proof mechanism): the editor is generated from the catalog.
 * A fixture catalog carrying a NOVEL event kind (weather.station / storm_warning) and a
 * NOVEL action (sound_alarm) — neither exists anywhere in the codebase — renders fully:
 * WHEN options, distinct-activity help, filters, typed IF rows with type-prefixed
 * operators, enum value selects, the action's schema and the {{...}} interpolation picker.
 * No code change accompanies this catalog; that is the point.
 */
import { fireEvent, render, screen, within } from "@testing-library/react";
import { useState } from "react";
import { describe, expect, it } from "vitest";

import { emptyDraft } from "./draft";
import { NOVEL_CATALOG } from "./testFixtures";
import { TriggerForm, type TriggerDraft } from "./TriggerForm";

function Harness() {
  const [draft, setDraft] = useState<TriggerDraft>(emptyDraft());
  return (
    <TriggerForm
      catalog={NOVEL_CATALOG}
      agents={[{ id: "a1", name: "Reviewer" }]}
      draft={draft}
      onChange={setDraft}
      errors={{}}
    />
  );
}

function pickNovelEvent() {
  fireEvent.change(screen.getByLabelText("Event"), {
    target: { value: "weather.station|storm_warning" },
  });
}

describe("catalog-driven editor", () => {
  it("offers the novel source's event, grouped by source", () => {
    render(<Harness />);
    const picker = screen.getByLabelText("Event");
    const group = within(picker).getByRole("group", { name: "weather.station" });
    expect(within(group).getByRole("option", { name: "Storm warning" })).toBeTruthy();
  });

  it("renders the novel event's activity chips, marking the help-carrying one distinct", () => {
    render(<Harness />);
    pickNovelEvent();
    const issued = screen.getByRole("button", { name: "issued" });
    const escalated = screen.getByRole("button", { name: "escalated" });
    expect(issued.hasAttribute("data-distinct")).toBe(false);
    expect(escalated.getAttribute("data-distinct")).toBe("true");
    // The helper text that says why renders below the chips.
    expect(screen.getByText(/this is where the loop lives/i)).toBeTruthy();
    // Chips multi-select.
    fireEvent.click(issued);
    fireEvent.click(escalated);
    expect(screen.getByRole("button", { name: "issued" }).getAttribute("aria-pressed")).toBe(
      "true",
    );
    expect(
      screen.getByRole("button", { name: "escalated" }).getAttribute("aria-pressed"),
    ).toBe("true");
  });

  it("renders the novel event's filters from FilterFields", () => {
    render(<Harness />);
    pickNovelEvent();
    expect(screen.getByLabelText("Regions filter")).toBeTruthy();
  });

  it("builds IF rows from the novel fields with type-prefixed operators and enum values", () => {
    render(<Harness />);
    pickNovelEvent();
    fireEvent.click(screen.getByRole("button", { name: "+ And" }));

    const fieldSelect = screen.getByLabelText("Field");
    expect(within(fieldSelect).getByRole("option", { name: "storm.wind_speed" })).toBeTruthy();

    // A number field offers only (number)-prefixed operators.
    fireEvent.change(fieldSelect, { target: { value: "storm.wind_speed" } });
    const opSelect = screen.getByLabelText("Operator");
    const ops = within(opSelect)
      .getAllByRole("option")
      .map((o) => o.textContent);
    expect(ops).toContain("(number) less than");
    expect(ops.every((t) => t?.startsWith("(number)"))).toBe(true);

    // An enum field with catalog values renders a value select carrying them.
    fireEvent.change(fieldSelect, { target: { value: "storm.severity" } });
    const valueSelect = screen.getByLabelText("Value");
    expect(within(valueSelect).getByRole("option", { name: "emergency" })).toBeTruthy();
  });

  it("renders the novel action's schema and its interpolation picker", () => {
    render(<Harness />);
    pickNovelEvent();
    const add = screen.getByLabelText("Add action");
    expect(within(add).getByRole("option", { name: "Sound the alarm" })).toBeTruthy();
    fireEvent.change(add, { target: { value: "sound_alarm" } });

    // The schema's enum param renders a select with its values.
    const volume = screen.getByLabelText("Volume");
    expect(within(volume).getByRole("option", { name: "loud" })).toBeTruthy();

    // The template param carries the {{...}} field picker listing the event's paths.
    fireEvent.click(screen.getByRole("button", { name: /insert a field/ }));
    const menu = screen.getByRole("menu", { name: "Interpolation fields" });
    fireEvent.click(within(menu).getByRole("menuitem", { name: "{{storm.name}}" }));
    expect((screen.getByLabelText("Message") as HTMLTextAreaElement).value).toBe(
      "{{storm.name}}",
    );
  });
});
