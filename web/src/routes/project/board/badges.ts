/*
 * Card badge logic (UI spec §5.3): badges appear ONLY when earned — a criteria progress row
 * only if criteria exist, a PR chip only if a PR is linked, the delegate → assignee row only
 * when at least one of the two is set. Never an empty badge slot.
 *
 * The card anatomy from the spec:
 *
 *   PAY-14                    ▲ needs you     key · status dot
 *   Add idempotency keys to charge API        title, 2 lines max
 *   ▸ 3/5 acceptance criteria                 progress, only if criteria exist
 *   [dev] ⟶ @spruce      +142 −18   #219      delegate → assignee, diff, PR
 *
 * The diff stat (+142 −18) has no data source until the forge integration reports it — it is
 * earned by having a diff, and no ticket has one yet, so it renders nowhere (not as an empty
 * slot). The ▲ needs-you corner badge is earned by a run in a needs-you state (S22).
 */
import type { Ticket } from "../../../lib/api/client";

export interface CardBadges {
  /** "3/5 acceptance criteria" — only when the ticket has criteria. */
  criteria: string | null;
  /** The bottom people row renders only when delegate or assignee is set. */
  delegate: string | null;
  assignee: string | null;
  hasPeopleRow: boolean;
  /** "#219" — only when a PR is linked. */
  pr: string | null;
}

export function cardBadges(t: Ticket): CardBadges {
  const delegate = t.delegate_agent_id;
  const assignee = t.assignee_id;
  return {
    criteria:
      t.criteria_total > 0 ? `${t.criteria_checked}/${t.criteria_total} acceptance criteria` : null,
    delegate,
    assignee,
    hasPeopleRow: delegate !== null || assignee !== null,
    pr: t.pr_number !== null ? `#${t.pr_number}` : null,
  };
}
