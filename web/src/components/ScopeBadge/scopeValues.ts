import type { AgentScope } from "../../lib/api/client";

/** The five agent_scope values in display order (schema CHECK, data model §5). */
export const SCOPE_VALUES: readonly AgentScope[] = [
  "always",
  "auto",
  "paths",
  "manual",
  "never",
];
