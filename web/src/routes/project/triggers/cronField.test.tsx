/*
 * S32: the cron expression rides the catalog FilterField mechanism. The form renders a
 * non-"-list" filter kind as a single-value input (commas stay inside the value — a cron
 * list like "0 9 * * 1,3" must survive), and the draft mapping routes the reserved "cron"
 * filter key to the API's dedicated `cron` column in both directions. The catalog below is
 * a fixture: like S29's novel source, no code in the form knows the schedule event exists.
 */
import { fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";
import { describe, expect, it } from "vitest";

import type { TriggerCatalog } from "../../../lib/api/client";
import { draftFromTrigger, draftToInput, emptyDraft } from "./draft";
import { makeTrigger, OPERATORS } from "./testFixtures";
import { TriggerForm, type TriggerDraft } from "./TriggerForm";

const SCHEDULE_CATALOG: TriggerCatalog = {
  sources: [
    {
      id: "schedule.cron",
      events: [
        {
          kind: "schedule",
          label: "Schedule",
          activity_types: [
            {
              value: "cron",
              label: "on a cron schedule",
              help: "Fires on this rule's cron expression, evaluated in UTC.",
            },
          ],
          filters: [{ key: "cron", kind: "cron", label: "Cron expression (UTC)" }],
          fields: [
            { path: "schedule.cron", type: "text" },
            { path: "schedule.fired_at", type: "text" },
          ],
          subject_key: "schedule:{{schedule.trigger_id}}",
        },
      ],
    },
  ],
  actions: [],
  operators: OPERATORS,
};

function Harness({ onDraft }: { onDraft?: (d: TriggerDraft) => void }) {
  const [draft, setDraft] = useState<TriggerDraft>({
    ...emptyDraft(),
    source_id: "schedule.cron",
    event: "schedule",
  });
  return (
    <TriggerForm
      catalog={SCHEDULE_CATALOG}
      agents={[]}
      draft={draft}
      onChange={(next) => {
        setDraft(next);
        onDraft?.(next);
      }}
      errors={{}}
    />
  );
}

describe("cron expression through the catalog filter mechanism", () => {
  it("renders the cron filter as a single-value input whose commas stay put", () => {
    let last: TriggerDraft | undefined;
    render(<Harness onDraft={(d) => (last = d)} />);
    const input = screen.getByLabelText("Cron expression (UTC) filter") as HTMLInputElement;
    expect(input.placeholder).toBe("0 9 * * 1-5");

    fireEvent.change(input, { target: { value: "0 9 * * 1,3" } });
    // A "-list" kind would have split on the comma; the cron kind must not.
    expect(last?.filters.cron).toEqual(["0 9 * * 1,3"]);
  });

  it("routes the reserved cron filter key to the API's cron field, out of filters", () => {
    const draft: TriggerDraft = {
      ...emptyDraft(),
      name: "standup",
      source_id: "schedule.cron",
      event: "schedule",
      activity_types: ["cron"],
      filters: { cron: ["0 9 * * 1-5"] },
    };
    const input = draftToInput(draft);
    expect(input.cron).toBe("0 9 * * 1-5");
    expect(input.filters).toEqual({});
  });

  it("clears the stored expression when the input was emptied", () => {
    const input = draftToInput({ ...emptyDraft(), filters: {} });
    expect(input.cron).toBe("");
  });

  it("surfaces a stored expression back into the filter input on load", () => {
    const tr = makeTrigger({
      source_id: "schedule.cron",
      event: "schedule",
      activity_types: ["cron"],
      filters: {},
      cron: "30 6 * * *",
    });
    const draft = draftFromTrigger(tr);
    expect(draft.filters.cron).toEqual(["30 6 * * *"]);
  });
});
