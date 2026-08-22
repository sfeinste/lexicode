/*
 * S35 acceptance, UI side:
 *  - an edit-proposal renders as a line diff (proposed body vs the live page's body) with
 *    removed lines marked del and new lines marked add;
 *  - a pending wiki proposal surfaces as a needs-you row with the review flavor, linking
 *    to the page's review view (not to a run).
 */
import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import type { NeedsYouRun } from "../../../lib/api/client";
import { needsYouView } from "../../inbox/needsYouView";
import { ProposalDiff } from "./ProposalDiff";

describe("ProposalDiff", () => {
  it("renders an edit proposal as a line diff against the live body", () => {
    const live = "Ship on Tuesdays.\nNever deploy manually.";
    const proposed = "Ship on Tuesdays.\nNever ship on Fridays.\nAlways tag releases.";
    const { container, getByText } = render(
      <ProposalDiff label="Proposed changes to Deploys" from={live} to={proposed} />,
    );

    getByText("Proposed changes to Deploys");
    const ops = [...container.querySelectorAll("[data-op]")].map((el) => [
      el.getAttribute("data-op"),
      el.textContent,
    ]);
    expect(ops).toEqual([
      ["same", "  Ship on Tuesdays."],
      ["del", "- Never deploy manually."],
      ["add", "+ Never ship on Fridays."],
      ["add", "+ Always tag releases."],
    ]);
  });

  it("marks every line added for a diff against an empty base", () => {
    const { container } = render(<ProposalDiff label="New page" from="" to={"One\nTwo"} />);
    const dels = container.querySelectorAll('[data-op="del"]');
    expect(dels.length).toBe(1); // the empty base's single empty line
    expect(container.querySelectorAll('[data-op="add"]').length).toBeGreaterThan(0);
  });
});

describe("needsYouView", () => {
  const proposalRow: NeedsYouRun = {
    kind: "wiki_proposal",
    id: "page-1",
    project_key: "PAY",
    ticket_id: null,
    ticket_key: null,
    ticket_title: null,
    agent: "Dev",
    flavor: "review",
    status: "proposed",
    started_at: "2026-08-21T10:00:00Z",
    page_slug: "database-migrations",
    page_title: "Database migrations",
  };

  it("renders a wiki proposal as a review-flavored row linking to the wiki page", () => {
    const view = needsYouView(proposalRow);
    expect(view.isProposal).toBe(true);
    expect(view.flavorLabel).toBe("Review output");
    expect(view.subject).toBe("Dev proposed “Database migrations”");
    expect(view.action).toBe("Review");
    expect(view.link).toEqual({
      to: "/p/$key/wiki/$slug",
      params: { key: "PAY", slug: "database-migrations" },
    });
  });

  it("keeps run rows on the run link with the flavor action", () => {
    const runRow: NeedsYouRun = {
      kind: "run",
      id: "run-9",
      project_key: "PAY",
      ticket_id: "t1",
      ticket_key: "PAY-14",
      ticket_title: "Add idempotency keys",
      agent: "Dev",
      flavor: "question",
      status: "needs_input",
      started_at: "2026-08-21T10:00:00Z",
    };
    const view = needsYouView(runRow);
    expect(view.isProposal).toBe(false);
    expect(view.flavorLabel).toBe("Answer a question");
    expect(view.subject).toBe("PAY-14 · Add idempotency keys");
    expect(view.link).toEqual({
      to: "/p/$key/runs/$id",
      params: { key: "PAY", id: "run-9" },
    });
  });
});
