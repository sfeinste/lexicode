/*
 * S38: the §8 activation moments show exactly once per project. localStorage-backed
 * (documented in activation.ts); jsdom provides a real localStorage here.
 */
import { beforeEach, describe, expect, it } from "vitest";

import { markMomentSeen, momentPending } from "./activation";

beforeEach(() => {
  localStorage.clear();
});

describe("activation moments (S38)", () => {
  it("is pending until marked, then never again", () => {
    expect(momentPending("first-completed-run", "PAY")).toBe(true);
    markMomentSeen("first-completed-run", "PAY");
    expect(momentPending("first-completed-run", "PAY")).toBe(false);
  });

  it("tracks per project and per moment independently", () => {
    markMomentSeen("first-completed-run", "PAY");
    expect(momentPending("first-completed-run", "OPS")).toBe(true);
    expect(momentPending("first-needs-input", "PAY")).toBe(true);
  });

  it("survives a reload (it is storage, not memory)", () => {
    markMomentSeen("first-needs-input", "PAY");
    // A fresh import cannot easily be simulated, but the read goes straight to
    // localStorage on every call — assert the stored key exists.
    expect(localStorage.getItem("lexicode-moment:first-needs-input:PAY")).not.toBeNull();
  });
});
