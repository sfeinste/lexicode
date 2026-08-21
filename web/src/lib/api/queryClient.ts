import { QueryClient } from "@tanstack/react-query";

import { isClientProblem } from "./client";

/*
 * TanStack Query defaults for the whole app.
 *
 * - staleTime 30s: the SSE stream is what makes data fresh (architecture §13); background
 *   refetch is a safety net, not the mechanism.
 * - retry: never retry a 4xx problem — a validation error or a 401 will not get better on
 *   the third attempt. Network errors and 5xx get two retries.
 */
export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 30_000,
      retry: (failureCount, error) => !isClientProblem(error) && failureCount < 2,
    },
    mutations: {
      retry: false,
    },
  },
});
