/*
 * S12 acceptance: converting an N-line selection creates exactly N sub-tickets with the
 * right titles. The preview dialog and the POST body both come from selectionToTitles, so
 * this is the function under test.
 */
import { describe, expect, it } from "vitest";

import { selectionToTitles } from "./subticketSelection";

describe("selectionToTitles", () => {
  it("turns an N-line selection into exactly N titles", () => {
    const selection = "Add idempotency keys\nRetry failed charges\nDocument the API";
    expect(selectionToTitles(selection)).toEqual([
      "Add idempotency keys",
      "Retry failed charges",
      "Document the API",
    ]);
  });

  it("strips list markers and checkbox syntax — an agent's plan converts cleanly", () => {
    const selection = [
      "- [ ] Add tests for the charge path",
      "- [x] Wire the retry queue",
      "* Bullet variant",
      "2. Numbered variant",
      "# Heading variant",
    ].join("\n");
    expect(selectionToTitles(selection)).toEqual([
      "Add tests for the charge path",
      "Wire the retry queue",
      "Bullet variant",
      "Numbered variant",
      "Heading variant",
    ]);
  });

  it("drops empty and whitespace-only lines — a 4-line selection with a blank makes 3", () => {
    expect(selectionToTitles("one\n\n   \ntwo\nthree")).toEqual(["one", "two", "three"]);
  });

  it("an empty selection previews zero sub-tickets", () => {
    expect(selectionToTitles("")).toEqual([]);
    expect(selectionToTitles("   \n  ")).toEqual([]);
  });
});
