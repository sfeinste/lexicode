/*
 * The one place a needs-you row's rendering is decided (S35): the same GET /inbox rows feed
 * the home strip, the left rail and /inbox, and since S35 they are discriminated by `kind` —
 * run rows link to the run, wiki-proposal rows (flavor review) link to the proposed page's
 * review view. Every consumer derives its subject line, action label and link target here
 * so the three renderings can never disagree.
 */
import type { NeedsYouRun } from "../../lib/api/client";
import { NEEDS_YOU_FLAVORS } from "../project/board/TicketCard";

/* The flavor's inline action, in words (interaction rule 1). */
const FLAVOR_ACTIONS: Record<string, string> = {
  question: "Answer",
  approval: "Approve",
  review: "View diff",
  failure: "View run",
};

export interface NeedsYouView {
  /** True for wiki-proposal rows: no acknowledge, link goes to the wiki page. */
  isProposal: boolean;
  /** The flavor in words ("Review output", "Answer a question", …). */
  flavorLabel: string;
  /** The subject line: "PAY-14 · Add idempotency keys", or the proposal's page title. */
  subject: string;
  /** The inline action label ("Answer", "Review", …). */
  action: string;
  /** The row's link target — a run page, or the proposal's wiki page. */
  link:
    | { to: "/p/$key/runs/$id"; params: { key: string; id: string } }
    | { to: "/p/$key/wiki/$slug"; params: { key: string; slug: string } };
  /**
   * Pull-request rows only: the PR's web URL. The primary action opens this (review
   * happens on the forge) while `link` still reaches the producing run — S36's "link to
   * PR + run".
   */
  href?: string;
  /**
   * Blocked-run rows (question/approval) answer from the row itself: the S24 respond
   * components render inline against this run id, no navigation (S36).
   */
  respondRunId?: string;
}

export function needsYouView(row: NeedsYouRun): NeedsYouView {
  const flavorLabel =
    NEEDS_YOU_FLAVORS[row.flavor as keyof typeof NEEDS_YOU_FLAVORS] ?? row.flavor;
  if (row.kind === "wiki_proposal") {
    return {
      isProposal: true,
      flavorLabel,
      subject: `${row.agent} proposed “${row.page_title ?? row.page_slug ?? "a wiki page"}”`,
      action: "Review",
      link: {
        to: "/p/$key/wiki/$slug",
        params: { key: row.project_key, slug: row.page_slug ?? "" },
      },
    };
  }
  if (row.kind === "pull_request") {
    return {
      isProposal: false,
      flavorLabel,
      subject:
        row.pr_number !== undefined && row.pr_number > 0
          ? `${row.agent} opened PR #${row.pr_number}`
          : `${row.agent} opened a pull request`,
      action: "Review PR",
      // The row links to the producing run; the action opens the PR itself.
      link: {
        to: "/p/$key/runs/$id",
        params: { key: row.project_key, id: row.run_id ?? "" },
      },
      href: row.url !== undefined && row.url !== "" ? row.url : undefined,
    };
  }
  const inline = row.flavor === "question" || row.flavor === "approval";
  return {
    isProposal: false,
    flavorLabel,
    subject:
      row.ticket_key !== null ? `${row.ticket_key} · ${row.ticket_title ?? ""}` : row.agent,
    action: FLAVOR_ACTIONS[row.flavor] ?? "Open",
    link: { to: "/p/$key/runs/$id", params: { key: row.project_key, id: row.id } },
    respondRunId: inline ? row.id : undefined,
  };
}

/**
 * The inbox sort (UI spec §5.10): approvals to the top ALWAYS, whatever their age —
 * everything else keeps the service order (question → failure → review, oldest first).
 * This is a rendering decision of /inbox; the home strip keeps the service's
 * question-first order (§5.1). Pure and stable, so it is testable and group-safe.
 */
export function sortForInbox(rows: readonly NeedsYouRun[]): NeedsYouRun[] {
  const approvals = rows.filter((r) => r.flavor === "approval");
  const rest = rows.filter((r) => r.flavor !== "approval");
  return [...approvals, ...rest];
}
