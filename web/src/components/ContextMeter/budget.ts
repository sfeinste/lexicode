/*
 * The ContextMeter's budget logic and copy, in a plain module so the component file only
 * exports components (react-refresh) and the vitest suite can assert the exact strings.
 */

/** The verbatim advice the amber state renders (implementation plan S34). */
export const CONTEXT_BUDGET_ADVICE =
  "cut what the agent can read from the code; keep pitfalls, rationale, and conventions that differ from defaults";

/** Whether the always-on total blows the budget. A non-positive threshold never flags. */
export function isOverBudget(alwaysTokens: number, thresholdTokens: number): boolean {
  return thresholdTokens > 0 && alwaysTokens > thresholdTokens;
}
