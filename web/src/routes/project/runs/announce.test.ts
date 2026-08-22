/*
 * S38 / UI spec §10: live regions announce run state transitions and step boundaries ONLY.
 * runAnnouncement is the entire policy — RunDetailPage feeds it (state, step_count,
 * current_step) and nothing else, so a streamed log line can never reach the announcer.
 */
import { describe, expect, it } from "vitest";

import { runAnnouncement, type AnnounceSnapshot } from "./announce";

const at = (
  state: AnnounceSnapshot["state"],
  stepCount: number,
  currentStep = "",
): AnnounceSnapshot => ({ state, stepCount, currentStep });

describe("runAnnouncement (§10 live-region policy)", () => {
  it("stays quiet on first observation — no announcement for merely opening the page", () => {
    expect(runAnnouncement(null, at("running", 3, "Editing files"))).toBeNull();
  });

  it("announces a state transition in the §4 vocabulary", () => {
    expect(runAnnouncement(at("running", 3), at("needs_input", 3))).toBe(
      "Run is now needs input",
    );
    expect(runAnnouncement(at("needs_input", 3), at("running", 3))).toBe("Run is now running");
    expect(runAnnouncement(at("running", 8), at("completed", 8))).toBe("Run is now completed");
  });

  it("announces a step boundary with the step's title", () => {
    expect(runAnnouncement(at("running", 3, "old"), at("running", 4, "Running tests"))).toBe(
      "Step 4: Running tests",
    );
    expect(runAnnouncement(at("running", 3), at("running", 4))).toBe("Step 4");
  });

  it("prefers the state transition when both change at once", () => {
    expect(runAnnouncement(at("running", 3), at("completed", 4))).toBe("Run is now completed");
  });

  it("says nothing when neither state nor step changed — a log-line append is silent", () => {
    // A streamed activity that starts no step: same state, same step count. The
    // current_step sentence may mutate as the run works; that alone must not announce.
    expect(runAnnouncement(at("running", 3, "Editing files"), at("running", 3, "Editing files")))
      .toBeNull();
    expect(
      runAnnouncement(at("running", 3, "Editing files"), at("running", 3, "Still editing")),
    ).toBeNull();
  });
});
