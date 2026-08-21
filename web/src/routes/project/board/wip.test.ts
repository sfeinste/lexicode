import { describe, expect, it } from "vitest";

import { wipDisplay } from "./wip";

describe("wipDisplay (§5.3: amber at the limit, red over it)", () => {
  it("no limit → no badge at all (badges only when earned)", () => {
    expect(wipDisplay(3, null)).toBeNull();
    expect(wipDisplay(0, null)).toBeNull();
  });

  it("under the limit is quiet", () => {
    expect(wipDisplay(3, 4)).toEqual({ label: "3/4", level: "under" });
    expect(wipDisplay(0, 4)).toEqual({ label: "0/4", level: "under" });
  });

  it("AT the limit turns amber", () => {
    expect(wipDisplay(4, 4)).toEqual({ label: "4/4", level: "at" });
  });

  it("OVER the limit turns red", () => {
    expect(wipDisplay(5, 4)).toEqual({ label: "5/4", level: "over" });
  });

  it("the S22 running-column form: queued runs append to the label", () => {
    expect(wipDisplay(4, 4, 2)).toEqual({ label: "4/4 · queued: 2", level: "at" });
    expect(wipDisplay(3, 4, 0)).toEqual({ label: "3/4", level: "under" });
  });
});
