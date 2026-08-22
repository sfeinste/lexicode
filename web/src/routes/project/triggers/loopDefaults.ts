/*
 * The data-model §6.1 default loop config: every layer on, depth 3, budget inheriting the
 * project ceiling. Mirrors domain.DefaultLoopConfig server-side.
 */
import type { LoopConfig } from "../../../lib/api/client";

export const DEFAULT_LOOP_CONFIG: LoopConfig = {
  actor_suppression: true,
  debounce_seconds: 90,
  cancel_in_progress: true,
  depth_limit: 3,
  daily_budget_cents: null,
};
