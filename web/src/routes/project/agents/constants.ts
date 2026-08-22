/*
 * Non-component exports for the §5.8 agent sections — verbatim spec strings, the model and
 * effort vocabularies (mirrors agents.KnownModels / KnownEfforts on the server), the autonomy
 * dial's stops, and the token heuristic. Separate file so sections.tsx exports only
 * components (react-refresh).
 */
import type { AgentAutonomy } from "../../../lib/api/client";

/** §5.8 verbatim: why the git identity matters. */
export const IDENTITY_LINE = "Events caused by this identity won't re-trigger this agent.";

/** UI spec §5.8 verbatim: unchecking a permission makes the action impossible, not
 * discouraged — enforcement, not guidance. */
export const ENFORCEMENT_SENTENCE =
  "A reviewer with edit unchecked cannot write code; that is stronger than telling it not to.";

export const MODELS = ["claude-opus-5", "claude-sonnet-5", "claude-haiku-4-5"];
export const EFFORTS = ["low", "medium", "high"];

/** §5.8's four-stop dial, ordered by increasing risk. */
export const AUTONOMY_STOPS: Array<{ value: AgentAutonomy; name: string; desc: string }> = [
  { value: "suggest", name: "Suggest", desc: "Plans only, never acts." },
  { value: "approve_each", name: "Approve each action", desc: "Every action waits for you." },
  {
    value: "auto_gates",
    name: "Auto with gates",
    desc: "Runs on its own; the plan and destructive actions still gate on you.",
  },
  { value: "auto", name: "Auto", desc: "Runs unattended within its permissions and limits." },
];

/** The documented chars/4 heuristic — must match agents.EstimateTokens on the server. */
export function estimateTokens(body: string): number {
  if (body.length === 0) return 0;
  return Math.max(1, Math.floor(body.length / 4));
}
